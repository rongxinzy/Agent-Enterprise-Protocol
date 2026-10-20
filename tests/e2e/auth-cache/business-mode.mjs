import assert from 'node:assert/strict';

export async function businessMode(context) {
  if (process.env.AEP_NATIVE_ACCESS_EMPLOYEE_BUSINESS === 'true') {
    assert(!context.realProviderKey, 'Employee business probe uses only the explicit mock provider');
    const {employeeBusinessAcceptance} = await import('./employee-business.mjs');
    return employeeBusinessAcceptance(context);
  }
  const {businessAcceptance} = await import('./business-acceptance.mjs');
  return businessAcceptance(context);
}
