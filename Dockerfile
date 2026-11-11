# Weft: one static binary in a small image.  docker build -t weft .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /weft ./cmd/weft
# the data folder, owned by the user the server runs as, so that a new volume mounted on it is writable
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /weft /weft
COPY --from=build --chown=nonroot:nonroot /data /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/weft"]
CMD ["serve", "-addr", ":8080", "-data", "/data"]
