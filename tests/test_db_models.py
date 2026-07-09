import uuid

from sqlalchemy import create_engine
from sqlalchemy.orm import Session

from app.db.base import Base
from app.db.models.config import CostAssumptionSet, MachineProfile, MaterialProfile
from app.pricing.models import Brand


def _memory_session() -> Session:
    engine = create_engine("sqlite://")
    Base.metadata.create_all(engine)
    return Session(engine)


def test_material_profile_roundtrip() -> None:
    session = _memory_session()
    mat = MaterialProfile(
        name="PLA Matte White", material_type="PLA", cost_per_kg=1000, colour="white"
    )
    session.add(mat)
    session.commit()

    loaded = session.get(MaterialProfile, mat.id)
    assert loaded is not None
    assert isinstance(loaded.id, uuid.UUID)
    assert loaded.is_active is True
    assert loaded.cost_per_kg == 1000.0


def test_cost_assumption_set_defaults_and_brand() -> None:
    session = _memory_session()
    cas = CostAssumptionSet(name="Gifting defaults", brand=Brand.GIFTING)
    session.add(cas)
    session.commit()

    loaded = session.get(CostAssumptionSet, cas.id)
    assert loaded is not None
    assert loaded.brand == Brand.GIFTING
    assert loaded.failure_pct == 0.06
    assert loaded.machine_hour_cost == 45.0
    assert loaded.is_default is False


def test_machine_profile_roundtrip() -> None:
    session = _memory_session()
    machine = MachineProfile(name="Bambu H2C", machine_hour_cost=45)
    session.add(machine)
    session.commit()

    loaded = session.get(MachineProfile, machine.id)
    assert loaded is not None
    assert loaded.name == "Bambu H2C"
