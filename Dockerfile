# Build stage — static binary, no CGO
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/server .

# Runtime stage — empty image, single binary, nothing else.
# The app is fully stateless: no files are written, no volumes needed.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/server /server
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/server"]
