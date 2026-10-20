import type {KnipConfig} from 'knip';

const config: KnipConfig = {
  workspaces: {
    '.': {
      entry: [
        // Documented CLI; not wired through package.json scripts.
        'scripts/render-higress-dashboard.mjs',
        // Compose bind-mount mock provider (deploy/compose/gateway.yaml, offline bundle).
        'tests/e2e/mock-openai.mjs',
        // Sidecar entry for offline-license Compose (tests/e2e/offline-license.compose.yaml).
        'tests/e2e/offline-access-proxy.mjs',
        // Optional publisher rehearsal; invoked manually per docs/credential-publisher.md.
        'tests/e2e/credential-publisher.mjs',
        // Dedicated Kind fixture; invoked manually per docs/gateway-operator-cutover*.md.
        'tests/e2e/gateway-publication-recovery.mjs',
        // Opt-in isolated component feasibility probe; see docs/auth-result-cache-feasibility.md.
        'tests/e2e/auth-result-cache.mjs',
        // ConfigMap-mounted disposable introspection service for the component probe.
        'tests/e2e/auth-cache/mock-introspection.mjs',
        // Native CR -> Operator acceptance; static source is ConfigMap-mounted.
        'tests/e2e/native-access-projection.mjs',
        'tests/e2e/auth-cache/native-policy-source.mjs',
      ],
      project: [
        'scripts/**/*.{js,mjs,cjs}',
        'tests/**/*.{js,mjs,cjs}',
        '.github/**/*.{js,cjs,mjs}',
      ],
      ignoreBinaries: [
        // CI and local release gates shell out to the Go toolchain directly.
        'go',
        'gofmt',
        // deadcode:py invokes the host Python interpreter from npm scripts.
        'python3',
      ],
    },
    'packages/aep-sdk-node': {
      entry: ['src/index.ts'],
      project: ['src/**/*.ts'],
      // OpenAPI-generated surface; many schema aliases are re-export metadata, not app imports.
      ignore: ['src/generated/**'],
    },
    'examples/node-agent': {
      entry: ['src/index.ts'],
      project: ['src/**/*.ts'],
    },
  },
};

export default config;
