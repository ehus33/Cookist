# @cookist/web

## 1.3.0

### Minor Changes

- 4ce8e45: **Resource cleanup.** Add confirmed resource deletion for ready and
  failed deployments, retryable cleanup failures, and serialized Terraform
  destruction using retained state. Successful cleanup frees a deployment slot;
  worker IAM permissions must be updated before use.

- **Deployment authorization.** Extend the owner-only gate with owner-default
  RBAC/ABAC for tenant-scoped deployments, server-managed expiring entitlements
  and quotas, and a permission-aware UI. Expired members retain authorized cleanup
  access. Payment checkout is not included.

## 1.2.1

### Patch Changes

- 646830b: **Hosted worker configuration.** Pass configured allowed origins to the
  container gateway so hosted deployment submissions work, and keep deployment
  state accessible only to the worker user.

## 1.2.0

### Minor Changes

- 646830b: **Managed deployments.** Add an operator-restricted Terraform deployment
  workflow for blank sites and business listings.

  Embed reusable S3 and CloudFront Terraform files and a provider lock file in the
  gateway, with isolated per-site variables and credential-free mock plan tests.

  Limit test-phase site creation to four deployments across all users sharing the
  worker state directory, including running, failed, and pending deployments.

  Add a separately bootstrapped, scoped local AWS worker role and require Cookist
  tags on new CloudFront distributions.

## 1.1.0

### Minor Changes

- d68e7cc: **Neutral workspace.** Remove the restaurant, ordering, map, and
  delivery features across the app and replace the previous interfaces with a
  neutral workspace.
