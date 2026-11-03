FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# The panel starts the Instagram bridge (Python + instagrapi) itself.
FROM python:3.12-slim
WORKDIR /app
COPY bridge/requirements.txt bridge/requirements.txt
RUN pip install --no-cache-dir -r bridge/requirements.txt
COPY --from=build /out/server ./server
COPY --from=build /src/web ./web
COPY --from=build /src/bridge/bridge.py ./bridge/bridge.py
ENV BRIDGE_PYTHON=python3
EXPOSE 8081
VOLUME ["/app/data"]
ENTRYPOINT ["/app/server"]
