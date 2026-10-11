# Model reference prices

`GET /aep/v1/admin/models/{modelId}/pricing` requires `models.read` and returns
`modelId`, `version`, nullable `pricing`, and nullable `updatedAt`. An existing
model with no saved configuration returns version `0` and null values.

`PUT` on the same path requires `models.write` and accepts `expectedVersion`
and `pricing`. Prices are exact nonnegative decimal strings (up to nine integer
digits and six decimal places), per **one million tokens**, with a three-letter
currency code. Input and output prices are required; cached input and a plain
text source note are optional. Zero is explicitly free; an unset price is unknown.
No exchange-rate conversion is performed. Local and cloud models use the same
reference-price configuration; this does not measure GPU or electricity costs.

Use the last returned version when saving. Concurrent edits return HTTP 409
`MODEL_PRICING_VERSION_CONFLICT`; reread before explicitly saving another edit.
To clear prices, send `pricing: null` with the current version. Clearing retains
the version history so stale writes cannot recreate removed prices.
Unknown or foreign-deployment models return 404. Malformed configuration returns
400 `INVALID_MODEL_PRICING`. Requests use the existing AEP bearer session and
`X-AEP-Protocol-Version: 1.0` header.

These endpoints store administrator-maintained reference configuration only.
They do not publish prices to Higress, change inference/quota behavior, calculate
costs, recalculate historical requests, or enable a native cost metric. The
monitoring UI must continue to report missing costs as unavailable until a
verified runtime billing source provides them.
