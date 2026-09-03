# Deployment identity transition

An AEP Control Service installation now has one stable Deployment identity.
Configure it with:

```text
AEP_DEPLOYMENT_ID=deployment-001
AEP_DEPLOYMENT_NAME=Zhiyuan office deployment
```

The service publishes `deploymentId` and a `deployment` descriptor from
`/aep/v1/metadata`, `/aep/v1/auth/methods`, and the current identity endpoint.
Login-issued access and model JWTs also carry `deployment_id`.

During the transition, `enterpriseId` remains accepted in authentication
requests and the legacy enterprise columns remain the storage tenant. A
deployment ID must match the configured installation; mismatched IDs are
rejected before credential lookup. This compatibility boundary is temporary
and is removed when the Session and legacy-model migrations land.

`AEP_LICENSE_DEPLOYMENT_ID` defaults to `AEP_DEPLOYMENT_ID` when it is not
provided, while the old `AEP_LICENSE_ENTERPRISE_ID` setting remains accepted
until the legacy schema is removed.
