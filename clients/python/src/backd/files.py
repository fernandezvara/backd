"""Helpers for files that need no request: a public file's address and what a document says of a version."""

from __future__ import annotations

from typing import Any

from .models import FileDetails

__all__ = ["public_url", "version_status"]


def public_url(file: FileDetails | None, version: str | None = None) -> str | None:
    """The permanent address of a public file, or of one of its made versions: where anyone reads it without
    credentials, to put straight into a page as soon as the document arrives. ``None`` for a file that is not
    public, and for a version that isn't ready yet (show a placeholder; :func:`version_status` tells why). Nothing is
    requested: the address is in the details the document came with."""
    if not file or not file.get("public"):
        return None
    if version is None:
        return file.get("url")
    v = file.get("versions", {}).get(version)
    return v.get("url") if v and v.get("status") == "ready" else None


def version_status(file: FileDetails, name: str) -> dict[str, Any]:
    """What a document says about a made version of one of its files, to show "processing…" or "no preview" and to
    reserve room for the picture: ``status``, ``ready``, and ``reason``, ``width`` and ``height`` when known.
    ``status`` is ``pending``, ``ready``, ``empty``, ``skipped`` or ``failed``, and ``undeclared`` for a version the
    file's details don't name (the field declares none, or it was declared after the file was uploaded)."""
    v = (file or {}).get("versions", {}).get(name)
    if not v:
        return {"status": "undeclared", "ready": False}
    out: dict[str, Any] = {"status": v["status"], "ready": v["status"] == "ready"}
    for k in ("reason", "width", "height"):
        if k in v:
            out[k] = v[k]
    return out
