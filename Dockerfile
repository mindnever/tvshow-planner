FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tvshow-planner ./cmd/tvshow-planner && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /tvshow-planner /tvshow-planner
COPY --from=build --chown=nonroot:nonroot /data /data
ENV DATABASE_PATH=/data/tvshow-planner.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/tvshow-planner"]
