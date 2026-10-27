#!/usr/bin/env python3
"""Package an already-built native launcher; never launch it or access user data."""
import argparse
import hashlib
import plistlib
import re
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", choices=("macos", "windows"), required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--identity", default="")
    parser.add_argument("--dmg", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version):
        parser.error("version must be a numeric release version, for example 0.1.0")
    args.output.mkdir(parents=True, exist_ok=True)
    name = f"alborz-{args.version}-{args.platform}-{args.arch}"
    archive = args.output / f"{name}.zip"
    if args.platform == "macos":
        app = args.output / f"macos-{args.arch}" / "Alborz.app"
        if app.is_symlink():
            parser.error("the generated app path must not be a symlink")
        if app.exists():
            shutil.rmtree(app)
        contents = app / "Contents"
        executable = contents / "MacOS" / "Alborz"
        executable.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(args.binary, executable)
        executable.chmod(0o755)
        resources = contents / "Resources"
        resources.mkdir()
        shutil.copy2(Path(__file__).with_name("Alborz.icns"), resources / "Alborz.icns")
        with (contents / "Info.plist").open("wb") as target:
            plistlib.dump({
                "CFBundleIdentifier": "org.alborzmail.Alborz",
                "CFBundleName": "Alborz",
                "CFBundleDisplayName": "Alborz",
                "CFBundleExecutable": "Alborz",
                "CFBundleIconFile": "Alborz.icns",
                "CFBundlePackageType": "APPL",
                "CFBundleShortVersionString": args.version,
                "CFBundleVersion": args.version,
                "LSUIElement": True,
                "LSMinimumSystemVersion": "13.0",
                "NSHighResolutionCapable": True,
            }, target)
        if args.identity:
            subprocess.run(["codesign", "--force", "--options", "runtime", "--timestamp",
                            "--sign", args.identity, str(app)], check=True)
            subprocess.run(["codesign", "--verify", "--strict", str(app)], check=True)
        # ditto preserves bundle metadata and executable permissions for Finder.
        subprocess.run(["ditto", "-c", "-k", "--sequesterRsrc", "--keepParent",
                        str(app), str(archive)], check=True)
        if args.dmg:
            with tempfile.TemporaryDirectory(prefix=f"dmg-{args.arch}-", dir=args.output) as temporary:
                stage = Path(temporary)
                subprocess.run(["ditto", str(app), str(stage / "Alborz.app")], check=True)
                (stage / "Applications").symlink_to("/Applications")
                subprocess.run(["hdiutil", "create", "-volname", "Alborz", "-srcfolder", str(stage),
                                "-ov", "-format", "UDZO", str(args.output / f"{name}.dmg")], check=True)
    else:
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as target:
            target.write(args.binary, "Alborz.exe")
    digest = hashlib.sha256()
    with archive.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    archive.with_suffix(".zip.sha256").write_text(f"{digest.hexdigest()}  {archive.name}\n")
    print(archive)


if __name__ == "__main__":
    main()
