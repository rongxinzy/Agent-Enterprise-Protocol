FROM mirror.gcr.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY services/gateway-reconciler ./services/gateway-reconciler
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aep-gateway-reconciler ./services/gateway-reconciler/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aep-gateway-reconciler /aep-gateway-reconciler
EXPOSE 8091
ENTRYPOINT ["/aep-gateway-reconciler"]
