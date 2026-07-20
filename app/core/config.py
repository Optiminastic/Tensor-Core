"""Application settings. Values are optional until the DB/auth are wired up."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    environment: str = "development"

    # Optional until the database is provisioned.
    database_url: str | None = None

    # Better Auth JWKS endpoint — FastAPI verifies frontend-issued JWTs against it.
    auth_jwks_url: str | None = None

    # Claims every access token must carry. `auth_issuer` is the frontend's
    # BETTER_AUTH_URL; `auth_audience` is this service. A token minted for
    # something else must not be accepted here, so both are verified.
    auth_issuer: str | None = None
    auth_audience: str = "tensor-core"

    # Shared secret for the frontend's server-to-server role lookup
    # (GET /internal/users/{id}/authz). No secret configured means the
    # internal API is closed, not open.
    internal_api_secret: str | None = None

    # How long a fetched JWKS is reused before refetching. Better Auth rotates
    # keys rarely, and PyJWKClient refetches on an unknown `kid` anyway, so this
    # only bounds staleness for keys that were removed.
    jwks_cache_seconds: int = 300

    # Allowed CORS origins (the Next.js frontend).
    cors_origins: list[str] = ["http://localhost:3000"]

    @property
    def sqlalchemy_url(self) -> str:
        """DATABASE_URL as a SQLAlchemy/psycopg URL."""
        if not self.database_url:
            raise RuntimeError("DATABASE_URL is not set")
        url = self.database_url
        if url.startswith("postgresql://"):
            url = url.replace("postgresql://", "postgresql+psycopg://", 1)
        return url


settings = Settings()
