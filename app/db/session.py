"""Database engine and session dependency.

The engine is created lazily on first use so importing the app (e.g. in tests
or CI, where the dependency is overridden) never requires a live DATABASE_URL.
"""

from collections.abc import Generator

from sqlalchemy import Engine, create_engine
from sqlalchemy.orm import Session, sessionmaker

from app.core.config import settings

_engine: Engine | None = None
_session_factory: sessionmaker[Session] | None = None


def _ensure_initialised() -> sessionmaker[Session]:
    global _engine, _session_factory
    if _session_factory is None:
        _engine = create_engine(settings.sqlalchemy_url, pool_pre_ping=True)
        _session_factory = sessionmaker(bind=_engine, autoflush=False, expire_on_commit=False)
    return _session_factory


def get_db() -> Generator[Session, None, None]:
    session_factory = _ensure_initialised()
    db = session_factory()
    try:
        yield db
    finally:
        db.close()
