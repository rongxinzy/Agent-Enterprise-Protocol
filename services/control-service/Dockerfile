FROM mirror.gcr.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY services/control-service ./services/control-service
COPY services/internal ./services/internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aep-control ./services/control-service/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aep-control /aep-control
EXPOSE 8080
ENTRYPOINT ["/aep-control"]
