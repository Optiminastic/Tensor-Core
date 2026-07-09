"""Application settings. Values are optional until the DB/auth are wired up."""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    environment: str = "development"

    # Optional until the database is provisioned.
    database_url: str | None = None

    # Better Auth JWKS endpoint — FastAPI verifies frontend-issued JWTs against it.
    auth_jwks_url: str | None = None

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
