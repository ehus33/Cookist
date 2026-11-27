package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/shopspring/decimal"
)

const databaseTenantTag = "CookistTenant"
const databaseScopeTag = "CookistDatabase"

var usdCostPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

type databaseCostReport struct {
	Period string
	Costs  map[string]string
}

type databaseCostSource interface {
	Ready(context.Context) error
	Report(context.Context, string, time.Time) (databaseCostReport, error)
}

type databaseCostAPI interface {
	ListCostAllocationTags(context.Context, *costexplorer.ListCostAllocationTagsInput, ...func(*costexplorer.Options)) (*costexplorer.ListCostAllocationTagsOutput, error)
	GetDimensionValues(context.Context, *costexplorer.GetDimensionValuesInput, ...func(*costexplorer.Options)) (*costexplorer.GetDimensionValuesOutput, error)
	GetCostAndUsage(context.Context, *costexplorer.GetCostAndUsageInput, ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
}

type awsDatabaseCosts struct {
	client         databaseCostAPI
	account, scope string
	mu             sync.Mutex
	readyUntil     time.Time
}

func databaseCostAmount(raw string) (decimal.Decimal, error) {
	if len(raw) > 80 || !usdCostPattern.MatchString(raw) {
		return decimal.Zero, errors.New("AWS returned an invalid USD cost")
	}
	return decimal.NewFromString(raw)
}

func databaseChargeCents(raw string) (int64, error) {
	amount, err := databaseCostAmount(raw)
	if err != nil {
		return 0, err
	}
	cents := amount.Mul(decimal.New(19, -1)).Shift(2).Round(0).BigInt()
	if !cents.IsInt64() {
		return 0, errors.New("database charge exceeds the supported amount")
	}
	return cents.Int64(), nil
}

func databaseCostPeriod(period string, now time.Time) (time.Time, time.Time, error) {
	start, err := time.Parse("2006-01", period)
	if err != nil || start.Format("2006-01") != period {
		return time.Time{}, time.Time{}, errors.New("database cost period must be YYYY-MM")
	}
	end := start.AddDate(0, 1, 0)
	// Wait a full week after the calendar month; an AWS report must also be non-estimated.
	if now.UTC().Before(end.AddDate(0, 0, 7)) {
		return time.Time{}, time.Time{}, errors.New("database costs have not reached the settlement window")
	}
	return start, end, nil
}

func (s *awsDatabaseCosts) Ready(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.readyUntil) {
		return nil
	}
	found := map[string]bool{}
	seenPages := map[string]bool{}
	var next *string
	for {
		out, err := s.client.ListCostAllocationTags(ctx, &costexplorer.ListCostAllocationTagsInput{
			TagKeys: []string{databaseTenantTag, databaseScopeTag}, NextToken: next,
		})
		if err != nil {
			return err
		}
		for _, tag := range out.CostAllocationTags {
			key := aws.ToString(tag.TagKey)
			if tag.Status == cetypes.CostAllocationTagStatusActive {
				found[key] = true
			}
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		cursor := aws.ToString(out.NextToken)
		if seenPages[cursor] {
			return errors.New("AWS repeated the cost tag cursor")
		}
		seenPages[cursor] = true
		next = out.NextToken
	}
	if !found[databaseTenantTag] || !found[databaseScopeTag] {
		return errors.New("activate CookistTenant and CookistDatabase AWS cost allocation tags before database billing")
	}
	s.readyUntil = time.Now().Add(15 * time.Minute)
	return nil
}

func (s *awsDatabaseCosts) Report(ctx context.Context, period string, now time.Time) (databaseCostReport, error) {
	report := databaseCostReport{Period: period, Costs: map[string]string{}}
	start, end, err := databaseCostPeriod(period, now)
	if err != nil {
		return report, err
	}
	if err := s.Ready(ctx); err != nil {
		return report, err
	}
	window := &cetypes.DateInterval{Start: aws.String(start.Format("2006-01-02")), End: aws.String(end.Format("2006-01-02"))}
	dimensions, err := s.client.GetDimensionValues(ctx, &costexplorer.GetDimensionValuesInput{
		TimePeriod: window, Dimension: cetypes.DimensionService, SearchString: aws.String("Amazon DynamoDB"),
		Context: cetypes.ContextCostAndUsage,
	})
	if err != nil {
		return report, err
	}
	serviceFound := false
	for _, value := range dimensions.DimensionValues {
		if aws.ToString(value.Value) == "Amazon DynamoDB" {
			serviceFound = true
		}
	}
	if !serviceFound {
		return report, errors.New("AWS has not reported DynamoDB billing data for this month")
	}
	filter := &cetypes.Expression{And: []cetypes.Expression{
		{Dimensions: &cetypes.DimensionValues{Key: cetypes.DimensionService, Values: []string{"Amazon DynamoDB"}}},
		{Dimensions: &cetypes.DimensionValues{Key: cetypes.DimensionLinkedAccount, Values: []string{s.account}}},
		{Tags: &cetypes.TagValues{Key: aws.String(databaseScopeTag), Values: []string{s.scope}}},
	}}
	var next *string
	seenPages := map[string]bool{}
	seenPeriod := false
	for {
		out, err := s.client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			TimePeriod: window, Granularity: cetypes.GranularityMonthly, Filter: filter,
			Metrics: []string{"NetUnblendedCost"}, GroupBy: []cetypes.GroupDefinition{{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(databaseTenantTag)}},
			NextPageToken: next,
		})
		if err != nil {
			return report, err
		}
		for _, result := range out.ResultsByTime {
			if result.Estimated || result.TimePeriod == nil || aws.ToString(result.TimePeriod.Start) != aws.ToString(window.Start) ||
				aws.ToString(result.TimePeriod.End) != aws.ToString(window.End) {
				return report, errors.New("AWS database costs are estimated or cover an unexpected period")
			}
			seenPeriod = true
			for _, group := range result.Groups {
				if len(group.Keys) != 1 || !strings.HasPrefix(group.Keys[0], databaseTenantTag+"$") {
					return report, errors.New("AWS returned an invalid database cost attribution")
				}
				tenant := strings.TrimPrefix(group.Keys[0], databaseTenantTag+"$")
				if tenant == "owner" {
					continue
				}
				if !tenantPattern.MatchString(tenant) {
					return report, errors.New("AWS database costs include an unattributed tenant")
				}
				metric, ok := group.Metrics["NetUnblendedCost"]
				if !ok || aws.ToString(metric.Unit) != "USD" {
					return report, errors.New("AWS database costs must be reported in USD")
				}
				amount, err := databaseCostAmount(aws.ToString(metric.Amount))
				if err != nil {
					return report, err
				}
				previous := decimal.Zero
				if raw, exists := report.Costs[tenant]; exists {
					previous, err = databaseCostAmount(raw)
					if err != nil {
						return report, err
					}
				}
				report.Costs[tenant] = previous.Add(amount).String()
			}
		}
		cursor := aws.ToString(out.NextPageToken)
		if cursor == "" {
			break
		}
		if seenPages[cursor] {
			return report, errors.New("AWS repeated the database cost cursor")
		}
		seenPages[cursor] = true
		next = out.NextPageToken
	}
	if !seenPeriod {
		return report, errors.New("AWS returned no database cost period")
	}
	return report, nil
}

func (r databaseCostReport) validate() error {
	if _, err := time.Parse("2006-01", r.Period); err != nil {
		return err
	}
	for tenant, amount := range r.Costs {
		if !tenantPattern.MatchString(tenant) || tenant == "owner" {
			return errors.New("invalid database cost tenant")
		}
		if _, err := databaseChargeCents(amount); err != nil {
			return fmt.Errorf("invalid database cost report: %w", err)
		}
	}
	return nil
}
