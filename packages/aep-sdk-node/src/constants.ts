export const AEP_PROTOCOL_VERSION = '1.0';

export const HttpMethod = {
  Delete: 'DELETE',
  Get: 'GET',
  Patch: 'PATCH',
  Post: 'POST',
  Put: 'PUT',
} as const;

export type HttpMethod = (typeof HttpMethod)[keyof typeof HttpMethod];

export const AepCapability = {
  PasswordAuth: 'password_auth',
  FederatedAuth: 'federated_auth',
  Skills: 'skills',
  Telemetry: 'telemetry',
  ControlEvents: 'control_events',
  ModelGateway: 'model_gateway',
  Credentials: 'credentials',
  Mcp: 'mcp',
  Plugins: 'plugins',
} as const;

export type AepCapability = (typeof AepCapability)[keyof typeof AepCapability];
