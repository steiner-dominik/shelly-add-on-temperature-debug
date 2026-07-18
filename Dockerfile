# Build stage — static binary, no CGO
FROM golang:1.26.5-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/server .

# Runtime stage — empty image, single binary, nothing else.
# The app is fully stateless: no files are written, no volumes needed.
#
# No USER directive: as a Home Assistant add-on the Supervisor mounts
# /data/options.json readable only by root, so a baked-in non-root user
# would break startup there. Standalone deployments should drop
# privileges at runtime instead (`user: "65534:65534"` in compose /
# `docker run --user 65534:65534`) — the app itself needs no privileges.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
