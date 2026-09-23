FROM mirror.gcr.io/library/golang:1.26.6-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY services/gateway-reconciler ./services/gateway-reconciler
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aep-gateway-reconciler ./services/gateway-reconciler/cmd/server \
    && mkdir -p /out/state && touch /out/state/.keep

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aep-gateway-reconciler /aep-gateway-reconciler
# Declare the runtime state dir with nonroot ownership: docker creates
# missing volume mountpoints as root, which made AEP_RECONCILER_OUTPUT_DIR
# unwritable for the nonroot process (desired-state apply failed with
# OUTPUT_WRITE_FAILED). Declaring the directory in the image lets the named
# volume copy it up with the right owner.
COPY --from=build --chown=65532:65532 /out/state/ /var/lib/aep-reconciler/
EXPOSE 8091
ENTRYPOINT ["/aep-gateway-reconciler"]
