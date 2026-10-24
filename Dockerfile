FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go config.yml ./
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /conductor .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 conductor
WORKDIR /app
COPY --from=build /conductor ./conductor
COPY config.yml ./config.yml
USER conductor
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
CMD ["./conductor"]
