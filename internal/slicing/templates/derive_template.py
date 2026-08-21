"""Derive a geometry-free machine settings template from a Bambu Studio project.

The Bambu Studio CLI refuses to slice for a multi-extruder machine when the
filament-to-nozzle map is supplied as flags (return_code -66). That map is not a
project setting: it lives on the *plate*, in Metadata/model_settings.config, as
filament_maps / filament_map_mode. The only way to hand it to the CLI is inside
a real project file.

This takes a project Bambu Studio itself wrote, so the structure is known-good,
and produces a template that carries only the settings:

  - the mesh in 3D/Objects/object_1.model becomes a placeholder cube, replaced
    per slice by internal/slicing/project3mf.go
  - MakerWorld / designer / licence metadata and every preview image are dropped,
    so the template carries none of the source model's identity
  - everything else is copied through byte-identical

Run once. The output is checked into the repo as a per-machine template.

  python derive_template.py <source-project>.3mf <template>.3mf
"""

import re
import sys
import zipfile

MESH_PATH = "3D/Objects/object_1.model"

# Metadata worth keeping in 3D/3dmodel.model. Everything else in the source is
# either the donor model's identity (Designer, Title, Description, License,
# DesignModelId...) or points at a preview image this template does not ship.
KEEP_METADATA = {
    "Application",
    "BambuStudio:3mfVersion",
    "CreationDate",
    "ModificationDate",
}

# The donor's own pictures and previews. All are optional in a project.
DROP_PREFIXES = ("Auxiliaries/",)
DROP_SUFFIXES = (".png", ".webp")
# plate_1.json is a slice *result* artefact carrying the donor plate name and
# bbox. Bambu Studio regenerates it; shipping a stale one would misdescribe us.
DROP_EXACT = ("Metadata/plate_1.json",)

# Bambu stores meshes centred on the origin and places them with the build item
# transform. A corner-origin placeholder reads as outside the print volume
# (return_code -50), so the placeholder follows the same convention. The object
# id and UUID match what the root model's <component> references.
PLACEHOLDER_CUBE = """<?xml version="1.0" encoding="UTF-8"?>
<model unit="millimeter" xml:lang="en-US" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:BambuStudio="http://schemas.bambulab.com/package/2021" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" requiredextensions="p">
 <metadata name="BambuStudio:3mfVersion">1</metadata>
 <resources>
  <object id="1" p:UUID="00010000-81cb-4c03-9d28-80fed5dfa1dc" type="model">
   <mesh>
    <vertices>
     <vertex x="-5" y="-5" z="-1.5"/>
     <vertex x="5" y="-5" z="-1.5"/>
     <vertex x="5" y="5" z="-1.5"/>
     <vertex x="-5" y="5" z="-1.5"/>
     <vertex x="-5" y="-5" z="8.5"/>
     <vertex x="5" y="-5" z="8.5"/>
     <vertex x="5" y="5" z="8.5"/>
     <vertex x="-5" y="5" z="8.5"/>
    </vertices>
    <triangles>
     <triangle v1="0" v2="2" v3="1"/>
     <triangle v1="0" v2="3" v3="2"/>
     <triangle v1="4" v2="5" v3="6"/>
     <triangle v1="4" v2="6" v3="7"/>
     <triangle v1="0" v2="1" v3="5"/>
     <triangle v1="0" v2="5" v3="4"/>
     <triangle v1="1" v2="2" v3="6"/>
     <triangle v1="1" v2="6" v3="5"/>
     <triangle v1="2" v2="3" v3="7"/>
     <triangle v1="2" v2="7" v3="6"/>
     <triangle v1="3" v2="0" v3="4"/>
     <triangle v1="3" v2="4" v3="7"/>
    </triangles>
   </mesh>
  </object>
 </resources>
 <build/>
</model>
"""


def clean_root_model(xml, _kept):
    """Strip donor metadata, keeping the structure Bambu Studio wrote."""

    def keep(match):
        return match.group(0) if match.group(1) in KEEP_METADATA else ""

    return re.sub(r'[ \t]*<metadata name="([^"]+)">.*?</metadata>\n', keep, xml, flags=re.S)


def clean_model_settings(xml, _kept):
    """Drop preview references and donor names, keep the filament map."""
    xml = re.sub(
        r'[ \t]*<metadata key="(thumbnail_file|thumbnail_no_light_file|top_file|pick_file)"[^>]*/>\n',
        "", xml)
    xml = re.sub(r'(<metadata key="name" value=")[^"]*(")', r"\g<1>plate\g<2>", xml)
    xml = re.sub(r'(<metadata key="source_file" value=")[^"]*(")', r"\g<1>plate.stl\g<2>", xml)
    # Face counts describe the placeholder, not the plate. The Go injector
    # rewrites them per slice; leaving stale numbers here would misreport.
    xml = re.sub(r'[ \t]*<metadata face_count="\d+"/>\n', "", xml)
    xml = re.sub(r'[ \t]*<mesh_stat [^>]*/>\n', "", xml)
    return xml


def clean_rels(xml, kept):
    """Drop relationships pointing at parts this template no longer ships.

    Matched on the target, not on a name pattern. An earlier version excluded
    every Relationship that did not mention "3dmodel.model", which also deleted
    the one pointing at /3D/Objects/object_1.model - leaving the mesh
    unreachable and every slice failing with an empty plate (return_code -50).
    """

    def keep(match):
        return match.group(0) if match.group(1).lstrip("/") in kept else ""

    return re.sub(r'[ \t]*<Relationship [^>]*Target="([^"]+)"[^>]*/>\n', keep, xml)


TRANSFORMS = {
    "3D/3dmodel.model": clean_root_model,
    "Metadata/model_settings.config": clean_model_settings,
    "_rels/.rels": clean_rels,
    "3D/_rels/3dmodel.model.rels": clean_rels,
}


def dropped(name):
    return name.startswith(DROP_PREFIXES) or name.endswith(DROP_SUFFIXES) or name in DROP_EXACT


def derive(src, dst):
    with zipfile.ZipFile(src) as zin, zipfile.ZipFile(dst, "w", zipfile.ZIP_DEFLATED) as zout:
        kept = {n for n in zin.namelist() if not dropped(n)}
        for name in zin.namelist():
            if dropped(name):
                continue
            if name == MESH_PATH:
                zout.writestr(name, PLACEHOLDER_CUBE)
                continue
            data = zin.read(name)
            transform = TRANSFORMS.get(name)
            if transform:
                data = transform(data.decode("utf-8"), kept).encode("utf-8")
            zout.writestr(name, data)


if __name__ == "__main__":
    derive(sys.argv[1], sys.argv[2])
    with zipfile.ZipFile(sys.argv[2]) as z:
        for n in z.namelist():
            print(f"{z.getinfo(n).file_size:>8}  {n}")
