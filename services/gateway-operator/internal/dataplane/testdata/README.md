# Synthetic Higress wire fixture

`higress-2.2.4-synthetic-wire.json` is a fixed, mock-only wire snapshot for the
OpenAI-compatible and Anthropic routes in `dump_test.go`. It contains disposable
provider values and synthetic cluster metadata, not a saved live config dump.
The bootstrap used the synthetic fixture builder; tests read the committed JSON
independently instead of rebuilding their observed wire data from `Compile`.
Changes to the renderer, compiled expectations, or pinned plugin artifacts must
therefore pass this fixed comparison or receive an explicit fixture review.

This regression protects the recorded wire shape and negative cases; it does not
prove that a new Higress version emits that shape. Keep the separate opt-in
`TestLocalKindLoadedConfiguration` acceptance against actual Higress/Envoy before
changing the supported gateway version. The existing generic Kind SSA scenario
installs compatible CRDs, not a running Higress controller and Envoy gateway.
