# TV Show Planner

In a group of people, different subgroups follow different TV shows together.
TV Show Planner answers one question: **given who is here right now, what can we watch?**

- Create a group ("Family", "Friends from work") and add its people. People are just names; they don't need accounts.
- Add shows and tick who watches each one together.
- On the **Tonight** page, tap everyone who is present. Shows are ranked:
  1. **Everyone's here** – the people present are exactly the show's watchers
  2. **Ready** – all watchers are present, plus someone who doesn't follow it
  3. **Missing …** – some watchers are absent (named)
- Optionally link one Jellyfin account per group. Shows are then picked from its library, with posters, the next episode, and "all caught up" shows pushed down.

Users log in through an OIDC provider (Authelia, Authentik, Keycloak, …). Any logged-in user can create groups and share them with other users through an invite link.

## Running

```sh
make run        # local, no OIDC: everyone is the user "dev"; http://localhost:8080
make test
make build      # bin/tvshow-planner, a single static binary
```

With Docker, copy `docker-compose.example.yml`, fill in the environment, and `docker compose up -d`.

## Configuration

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `SECRET_KEY` | yes | | At least 16 characters. Encrypts the stored Jellyfin token; changing it means re-linking Jellyfin. |
| `OIDC_ISSUER` | yes | | Issuer URL, e.g. `https://auth.example.com` (discovery is used) |
| `OIDC_CLIENT_ID` | yes | | |
| `OIDC_CLIENT_SECRET` | for confidential clients | | |
| `PUBLIC_BASE_URL` | yes in production | `http://localhost:8080` | Public URL of the app; the redirect URI is `<PUBLIC_BASE_URL>/auth/callback` |
| `DATABASE_PATH` | no | `tvshow-planner.db` | SQLite file (`/data/tvshow-planner.db` in the Docker image) |
| `LISTEN_ADDR` | no | `:8080` | |
| `DEV_AUTH_USER` | no | | **Development only.** Disables OIDC and logs everyone in as this user. |

### Authelia client (starting point)

```yaml
identity_providers:
  oidc:
    clients:
      - client_id: tvshow-planner
        client_name: TV Show Planner
        client_secret: '<digest of OIDC_CLIENT_SECRET>'
        public: false
        authorization_policy: two_factor
        require_pkce: true
        pkce_challenge_method: S256
        redirect_uris:
          - https://tv.example.com/auth/callback
        scopes: [openid, profile, email]
```

## Jellyfin

A manager links the group's account under **Settings** with the server address, username and password. The password is used once to obtain an access token and is not stored; the token is stored encrypted. The app only reads from Jellyfin (library, watched counts, next-up, posters) and proxies posters, so browsers never need to reach Jellyfin themselves. Progress refreshes when the Tonight page is opened and the cache is older than 10 minutes, or on demand.

## Layout

```
cmd/tvshow-planner   entry point
internal/config      environment configuration
internal/db          SQLite store + embedded goose migrations
internal/auth        OIDC login, sessions
internal/planner     ranking logic (pure, unit-tested)
internal/jellyfin    Jellyfin API client
internal/web         handlers, templates, static assets (htmx)
```
