# SPDX-FileCopyrightText: 2026 trippwill
# SPDX-License-Identifier: MPL-2.0

from pathlib import Path

from mkdocs.structure.files import File


def on_files(files, *, config):
    brand = Path(__file__).resolve().parents[2] / "internal/transport/admin/assets/brand"
    for source, destination in (
        ("wake-logo-reversed.svg", "assets/wake-logo-reversed.svg"),
        ("favicon.ico", "img/favicon.ico"),
    ):
        if existing := files.get_file_from_path(destination):
            files.remove(existing)
        files.append(File.generated(config, destination, abs_src_path=str(brand / source)))
    return files
