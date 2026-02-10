# Docker Compose Example

Run the dummy metrics producer and madVisor side by side.

## Usage

```bash
# Build and start both containers
docker compose up --build

# In a separate terminal, attach to the visualizer
docker attach $(docker compose ps -q madvisor)

# Stop
docker compose down
```

## Notes

- The `madvisor` container uses `stdin_open: true` and `tty: true` so you can attach to it interactively.
- `METRIC_TARGETS` points to `dummy-app:8080` using Docker's built-in DNS resolution.
- Press `Q` or `ESC` to quit the visualizer (detaches the TTY).
