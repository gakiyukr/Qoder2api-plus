FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/qoder-proxy ./cmd/qoder-proxy

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/qoder-proxy /usr/local/bin/qoder-proxy
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/qoder-proxy"]
CMD ["serve", "--config", "/app/config.json"]
