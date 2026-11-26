package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type accessMembership struct {
	TenantID string `json:"tenantId"`
	Role     string `json:"role"`
}

type accessTenant struct {
	SubscriptionStatus string `json:"subscriptionStatus"`
	ValidUntil         string `json:"validUntil,omitempty"`
	DeploymentLimit    int    `json:"deploymentLimit"`
}

type accessPolicy struct {
	Version int                         `json:"version"`
	Members map[string]accessMembership `json:"members"`
	Tenants map[string]accessTenant     `json:"tenants"`
}

type deploymentAccess struct {
	ActorID          string `json:"-"`
	TenantID         string `json:"tenantId,omitempty"`
	Role             string `json:"role"`
	Subscribed       bool   `json:"subscribed"`
	Limit            int    `json:"deploymentLimit"`
	CanList          bool   `json:"canList"`
	CanCreate        bool   `json:"canCreate"`
	CanDelete        bool   `json:"canDelete"`
	PaymentRequired  bool   `json:"paymentRequired"`
	CanCheckout      bool   `json:"canCheckout"`
	CanManageBilling bool   `json:"canManageBilling"`
}

var tenantPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func localSubscriptionBypass(mode, host string) (bool, error) {
	switch mode {
	case "hosted":
		return false, nil
	case "local":
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return false, errors.New("local access mode requires HOST to be an explicit loopback IP address")
		}
		return true, nil
	default:
		return false, errors.New("COOKIST_DEPLOYMENT_ACCESS_MODE must be hosted or local")
	}
}

func loadAccessPolicy(filename string) (accessPolicy, error) {
	var policy accessPolicy
	if filename == "" {
		return policy, nil
	}
	if !filepath.IsAbs(filename) {
		return policy, errors.New("deployment access file must be an absolute path")
	}
	file, err := os.Open(filename)
	if err != nil {
		return policy, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return policy, err
	}
	if len(data) > 1024*1024 {
		return policy, errors.New("deployment access file exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return policy, errors.New("deployment access file contains trailing data or exceeds the size limit")
	}
	if policy.Version != 1 || policy.Members == nil || policy.Tenants == nil {
		return policy, errors.New("deployment access file requires version 1, members and tenants")
	}
	for id, tenant := range policy.Tenants {
		if !tenantPattern.MatchString(id) || id == "owner" || tenant.DeploymentLimit < 1 ||
			tenant.DeploymentLimit > maxTestDeployments {
			return policy, errors.New("invalid tenant ID or deployment limit")
		}
		switch tenant.SubscriptionStatus {
		case "active":
			if _, err := time.Parse(time.RFC3339, tenant.ValidUntil); err != nil {
				return policy, errors.New("active subscription requires an RFC3339 validUntil")
			}
		case "inactive":
			if tenant.ValidUntil != "" {
				return policy, errors.New("inactive subscription must not have validUntil")
			}
		default:
			return policy, errors.New("subscriptionStatus must be active or inactive")
		}
	}
	for actor, member := range policy.Members {
		if actor == "" {
			return policy, errors.New("membership requires a verified actor ID")
		}
		if _, ok := policy.Tenants[member.TenantID]; !ok {
			return policy, errors.New("membership references an unknown tenant")
		}
		switch member.Role {
		case "customer_admin", "deployer", "viewer":
		default:
			return policy, fmt.Errorf("invalid membership role %q", member.Role)
		}
	}
	return policy, nil
}

func (w *deploymentWorker) accessFor(actor string) (deploymentAccess, error) {
	policy, err := loadAccessPolicy(w.accessFile)
	if err != nil {
		return deploymentAccess{}, err
	}
	access := deploymentAccess{ActorID: actor, Role: "none", PaymentRequired: !w.localAccess}
	if actor == w.owner {
		return deploymentAccess{ActorID: actor, TenantID: "owner", Role: "platform_owner",
			Subscribed: true, Limit: maxTestDeployments, CanList: true, CanCreate: true, CanDelete: true}, nil
	}
	member, ok := policy.Members[actor]
	var tenant accessTenant
	if !ok {
		if !w.selfServiceBilling || w.billing == nil || w.localAccess {
			return access, nil
		}
		if !strings.HasPrefix(actor, "cognito:") {
			return deploymentAccess{}, errors.New("self-service billing requires a verified Cognito actor")
		}
		digest := sha256.Sum256([]byte(actor))
		id := fmt.Sprintf("self-%x", digest[:28])
		if _, exists := policy.Tenants[id]; exists {
			return deploymentAccess{}, errors.New("self-service tenant conflicts with administrator-managed access")
		}
		member = accessMembership{TenantID: id, Role: "customer_admin"}
		tenant = accessTenant{SubscriptionStatus: "inactive", DeploymentLimit: 1}
	} else {
		tenant = policy.Tenants[member.TenantID]
	}
	subscribed := false
	if tenant.SubscriptionStatus == "active" {
		expires, err := time.Parse(time.RFC3339, tenant.ValidUntil)
		if err != nil {
			return deploymentAccess{}, err
		}
		subscribed = time.Now().Before(expires)
	}
	canManage := member.Role == "customer_admin" || member.Role == "deployer"
	canCheckout, canManageBilling := false, false
	if w.billing != nil && !w.localAccess {
		record, err := w.billing.read(member.TenantID)
		if err != nil {
			return deploymentAccess{}, err
		}
		subscribed = record.PriceID == w.billing.price && record.ValidUntil > time.Now().Unix()
		canCheckout = member.Role == "customer_admin" && !record.HasSubscription
		canManageBilling = member.Role == "customer_admin" && record.CustomerID != ""
	}
	return deploymentAccess{ActorID: actor, TenantID: member.TenantID, Role: member.Role,
		Subscribed: subscribed, Limit: tenant.DeploymentLimit, CanList: true,
		CanCreate: canManage && (subscribed || w.localAccess), CanDelete: canManage,
		PaymentRequired: !subscribed && !w.localAccess,
		CanCheckout:     canCheckout, CanManageBilling: canManageBilling}, nil
}

func (a deploymentAccess) check(action string, d *deployment) error {
	if d != nil && a.Role != "platform_owner" && d.TenantID != a.TenantID {
		return &deploymentError{404, "not_found", "Deployment not found."}
	}
	switch action {
	case "list":
		if a.CanList {
			return nil
		}
	case "create":
		if a.CanCreate {
			return nil
		}
		if (a.Role == "deployer" || a.Role == "customer_admin") && !a.Subscribed {
			return &deploymentError{403, "subscription_required", "An active subscription is required to create deployments. Existing resources can still be viewed and cleaned up."}
		}
	case "delete":
		if a.CanDelete {
			return nil
		}
	}
	return &deploymentError{403, "forbidden", "Your account does not have permission for this deployment action."}
}

func (w *deploymentWorker) ownedDeployment(d deployment) (deployment, error) {
	// Legacy deployment state belongs only to the configured platform owner.
	if d.TenantID == "" && d.CreatedBy == "" {
		d.TenantID, d.CreatedBy = "owner", w.owner
	}
	if !tenantPattern.MatchString(d.TenantID) || d.CreatedBy == "" {
		return d, errors.New("deployment ownership metadata is invalid")
	}
	return d, nil
}
