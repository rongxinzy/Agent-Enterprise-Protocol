-- Split deployments advertise the agent control protocol endpoint through
-- service metadata (agentControl.baseUrl); the runtime override lives here
-- next to the model gateway override.
ALTER TABLE deployment_settings ADD COLUMN IF NOT EXISTS agent_control_base_url text;
