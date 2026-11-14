# The build stage always runs on the builder's own architecture and
# cross-compiles for the target, so multi-arch builds need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# TARGETVARIANT is "v7" for linux/arm/v7 and empty otherwise.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /tvshow-planner ./cmd/tvshow-planner && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /tvshow-planner /tvshow-planner
COPY --from=build --chown=nonroot:nonroot /data /data
ENV DATABASE_PATH=/data/tvshow-planner.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/tvshow-planner"]
