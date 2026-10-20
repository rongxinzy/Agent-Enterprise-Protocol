package gatewaypolicy

// PresenceRego is the native OPA equivalent of Headers. It only represents
// subject presence; Higress/Redis remain responsible for counting/enforcement.
// Callers supply policy_subjects = [{kind, id}, ...] from trusted grants and
// requested_model from the validated request body. Keep key encoding identical
// to key()/subjectHeader(), including base64url without padding.
const PresenceRego = `
policy_headers[header] = "1" {
  subject := policy_subjects[_]
  key := concat(".", [subject.kind, base64url.encode_no_pad(subject.id), "all"])
  header := concat("", ["x-aep-limit-", crypto.sha256(key)])
}
policy_headers[header] = "1" {
  subject := policy_subjects[_]
  model := concat("", ["m.", base64url.encode_no_pad(requested_model)])
  key := concat(".", [subject.kind, base64url.encode_no_pad(subject.id), model])
  header := concat("", ["x-aep-limit-", crypto.sha256(key)])
}
`
