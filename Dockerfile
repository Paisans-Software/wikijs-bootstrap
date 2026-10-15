# The Wiki.js bootstrap's image: one static binary on distroless. It runs as
# uid 1000, the Wiki.js image's node user, so it may signal that process
# through the process namespace it shares with it.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The heartbeat's User-Agent names it.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /wikijs-bootstrap ./cmd/wikijs-bootstrap

FROM gcr.io/distroless/static-debian12
COPY --from=build /wikijs-bootstrap /wikijs-bootstrap
USER 1000:1000
EXPOSE 8090
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/wikijs-bootstrap", "healthcheck"]
ENTRYPOINT ["/wikijs-bootstrap"]
