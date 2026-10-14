# Team default roles

Team create, get, list and update APIs support `defaultRoleIds` (up to 64
distinct role IDs in the same deployment). Existing clients may omit it.
On PATCH, omission preserves the defaults and `[]` clears them.

Effective roles combine explicit user bindings and default roles inherited
from enabled teams of which the user is a direct member. Only enabled roles
confer permissions. Changes apply to existing and future members, and leaving
or disabling a team removes its inherited roles without altering explicit
user bindings. Parent teams do not implicitly grant roles. Default roles do
not set `users.is_admin`.

Management API authorization, current identity/session role claims, model,
Skill and Credential grants, role-targeted events, data scopes and gateway
membership snapshots all use the effective role set. User management `roleIds`
continue to describe explicit bindings, so editing a user never turns an
inherited role into a permanent grant. Already issued model JWTs retain their
claims until their existing short expiry; new tokens use current roles.

Default role changes require the existing Team write permission and the same
delegation checks as explicit role grants. Delegated operators cannot grant
built-in roles or permissions they do not hold. Joining a team or re-enabling
it checks its defaults too. Team-to-role foreign keys prevent invalid tenant
references; deleting a role still used as a team default is rejected.
