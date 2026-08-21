FROM mirror.gcr.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY services/gateway-authorizer ./services/gateway-authorizer
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aep-gateway-authorizer ./services/gateway-authorizer/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aep-gateway-authorizer /aep-gateway-authorizer
EXPOSE 8090
ENTRYPOINT ["/aep-gateway-authorizer"]
