# TV Show Planner

In a group of people, different subgroups follow different TV shows together.
TV Show Planner answers one question: **given who is here right now, what can we watch?**

- Log in with your Jellyfin account (password or Quick Connect).
- Create a group ("Family", "Friends from work") and add its people. People are just names; they don't need accounts.
- Add shows from your Jellyfin library and tick who watches each one together.
- On the **Tonight** page, tap everyone who is present. Shows are ranked:
  1. **Everyone's here** – the people present are exactly the show's watchers
  2. **Ready** – all watchers are present, plus someone who doesn't follow it
  3. **Missing …** – some watchers are absent (named)

  Each show has its poster and next episode; shows with nothing left to watch are pushed down.

## Accounts and Jellyfin

The app works with one Jellyfin server, set with `JELLYFIN_URL`. Jellyfin is the only login: there are no separate accounts or passwords.

- **Password login** sends the username and password to Jellyfin once. The password is not stored.
- **Quick Connect** shows a code to enter in a Jellyfin app where you are already signed in. It must be enabled on the server.
- The access token Jellyfin issues is stored encrypted with `SECRET_KEY`.

**A group reads Jellyfin as its owner**, the user who created it. The library, posters and watched progress the group shows are the owner's, whoever is looking at it. An owner can share a group with other Jellyfin users through an invite link; they manage the same group and see the same progress. If the owner's Jellyfin token stops working (for example the device was removed in Jellyfin), the group keeps its cached data and says so until the owner logs in again.

The app only reads from Jellyfin (library, watched counts, next-up, posters) and proxies posters, so browsers never need to reach Jellyfin themselves. Progress refreshes when the Tonight page is opened and the cache is older than 10 minutes, or on demand.

## Running

```sh
make run JELLYFIN_URL=http://localhost:8096   # http://localhost:8080
make test
make build      # bin/tvshow-planner, a single static binary
make generate   # regenerate the sqlc query code after changing SQL
```

With Docker, copy `docker-compose.example.yml`, fill in the environment, and `docker compose up -d`.

A GitHub workflow (`.github/workflows/docker.yml`) publishes the image to `ghcr.io/<owner>/tvshow-planner` for amd64, arm64 and armv7: `latest` from the default branch, and version tags from `v*` git tags.

## Configuration

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `JELLYFIN_URL` | yes | | Address of the Jellyfin server, as reachable from this app, e.g. `http://jellyfin:8096` |
| `SECRET_KEY` | yes | | At least 16 characters. Encrypts the stored Jellyfin tokens; changing it means everyone logs in again. |
| `PUBLIC_BASE_URL` | yes in production | `http://localhost:8080` | Public URL of the app, used for invite links |
| `DATABASE_PATH` | no | `tvshow-planner.db` | SQLite file (`/data/tvshow-planner.db` in the Docker image) |
| `LISTEN_ADDR` | no | `:8080` | |

## Layout

```
cmd/tvshow-planner   entry point
internal/config      environment configuration
internal/db          SQLite store: goose migrations, queries.sql, sqlc-generated code (make generate)
internal/auth        sessions
internal/planner     ranking logic (pure, unit-tested)
internal/jellyfin    Jellyfin API client (login, Quick Connect, library)
internal/web         handlers, templates, static assets (htmx)
```

## License

[MIT](LICENSE) © Vladimir Zidar. The bundled htmx (`internal/web/static/htmx.min.js`) is under its own license, Zero-Clause BSD.
