"""Databases and collections. Source of ``_sync/data.py``."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator, Mapping, Sequence
from typing import TYPE_CHECKING, Any, Generic, Literal, TypeVar, cast, overload

from ..models import AggregateResult, BatchOperation, Page
from ..query import Where, csv, if_match_value, where_json

if TYPE_CHECKING:
    from .client import AsyncClient

__all__ = ["AsyncCollection", "AsyncDatabase"]

D = TypeVar("D")
Deleted = Literal["only", "include"]


class AsyncDatabase:
    """A database of the realm: ``client.db(name)``."""

    def __init__(self, client: AsyncClient, name: str) -> None:
        self._client = client
        self.name = name

    @overload
    def collection(self, name: str) -> AsyncCollection[Any]: ...
    @overload
    def collection(self, name: str, doc: type[D]) -> AsyncCollection[D]: ...
    def collection(self, name: str, doc: type[Any] | None = None) -> AsyncCollection[Any]:
        """A collection of this database. ``doc`` is the (generated) document type of the collection; it only
        tells the type checker what the reads return."""
        return AsyncCollection(self._client, self.name, name)

    async def batch(
        self,
        operations: Sequence[BatchOperation],
        *,
        idempotency_key: str | None = None,
        team: str | None = None,
        org: str | None = None,
    ) -> list[dict[str, Any]]:
        """Creates, replaces, patches and deletes documents across this database's collections in one MongoDB
        transaction: either every operation applies, or none does (up to 100 operations). Fails with a
        :class:`BackdError` naming the first operation that failed; a version mismatch (``if_match``) is a
        :class:`VersionMismatchError`, same as a single write. Returns one entry per operation, in order."""
        ops: list[dict[str, Any]] = []
        for op in operations:
            body = {k: v for k, v in op.items() if k != "if_match"}
            if "if_match" in op:
                body["if_match"] = if_match_value(op["if_match"])
            ops.append(body)
        res = await self._client.request(
            "POST",
            [self.name, "_batch"],
            body={"operations": ops},
            query=_scope(team, org),
            headers=_idempotency(idempotency_key),
        )
        return cast("list[dict[str, Any]]", cast(dict[str, Any], res.data)["results"])


class AsyncCollection(Generic[D]):
    """A collection: ``client.db(db).collection(name)``. ``D`` is the type of its documents."""

    def __init__(
        self, client: AsyncClient, database: str, name: str, team: str | None = None, org: str | None = None
    ) -> None:
        self._client = client
        self.path = [database, name]
        self._team = team
        self._org = org

    def in_team(self, team: str) -> AsyncCollection[D]:
        """The same collection scoped to a team of the session's organization: ``create`` puts documents in that
        team and ``list`` and ``iterate`` show only its documents (the read rule still decides what is seen). The
        caller must belong to the team or hold a team role in it: a create fails with
        :class:`NotTeamMemberError` otherwise."""
        return AsyncCollection(self._client, self.path[0], self.path[1], team, self._org)

    def in_org(self, org: str) -> AsyncCollection[D]:
        """The same collection with the organization a create is made in named: for the administrators' data
        route, which creates in any active organization. A session works in the organization it was opened for,
        so it names none."""
        return AsyncCollection(self._client, self.path[0], self.path[1], self._team, org)

    def _scope(self, team: str | None = None, org: str | None = None) -> dict[str, str]:
        return _scope(team if team is not None else self._team, org if org is not None else self._org)

    # -- reads -----------------------------------------------------------------------------

    async def list(
        self,
        *,
        where: Where | str | None = None,
        order_by: str | Sequence[str] | None = None,
        limit: int | None = None,
        skip: int | None = None,
        after: str | None = None,
        count: bool = False,
        deleted: Deleted | None = None,
        file_links: bool = False,
        expand: str | None = None,
        language: str | None = None,
    ) -> Page[D]:
        """One page of documents the caller may read.

        ``where`` is a filter in backd's query language (a dict, or its JSON text); ``order_by`` lists fields,
        ``-`` prefix for descending (``"-_meta.created_at"``); ``limit`` is 1 to 100 (default 20); ``after`` is the
        ``next_cursor`` of the previous page and can't be combined with ``skip``; ``count`` also returns
        ``total``; ``file_links`` adds ``url`` and ``expires_at`` to every file; ``expand`` names the references
        to return in ``_expanded`` (build it with :func:`backd.expand`; never sent unless you set it);
        ``deleted`` is ``"only"`` (the trash) or ``"include"`` in a collection with ``soft_delete``;
        ``language`` is the language a ``$search`` is stemmed in."""
        query: dict[str, Any] = {
            "where": where_json(where),
            "order_by": csv(order_by),
            "limit": limit,
            "skip": skip,
            "after": after,
            "count": True if count else None,
            "deleted": deleted,
            "file_links": True if file_links else None,
            "expand": expand,
            "language": language,
            **self._scope(),
        }
        res = await self._client.request("GET", self.path, query=query)
        return _page(res.data)

    async def search(
        self,
        text: str,
        *,
        where: Where | str | None = None,
        limit: int | None = None,
        skip: int | None = None,
        count: bool = False,
        deleted: Deleted | None = None,
        file_links: bool = False,
        expand: str | None = None,
        language: str | None = None,
    ) -> Page[D]:
        """Full-text search, best match first: a ``list`` whose ``where`` carries ``text`` as ``$search`` and whose
        order is ``_relevance``, with each document's score in ``_meta.score``. ``text`` is words, ``"quoted
        phrases"`` and ``-excluded`` words. Pages with ``skip``, through the first 1,000 results.
        :class:`SearchNotEnabledError` when the collection has no text index."""
        if not isinstance(text, str) or not text.strip():
            raise TypeError("backd: search needs the text to search for")
        base = json.loads(where) if isinstance(where, str) else dict(where or {})
        return await self.list(
            where={**base, "$search": text},
            order_by="_relevance",
            limit=limit,
            skip=skip,
            count=count,
            deleted=deleted,
            file_links=file_links,
            expand=expand,
            language=language,
        )

    async def iterate(
        self,
        *,
        where: Where | str | None = None,
        order_by: str | Sequence[str] | None = None,
        limit: int = 100,
        after: str | None = None,
        deleted: Deleted | None = None,
        file_links: bool = False,
        expand: str | None = None,
        language: str | None = None,
    ) -> AsyncIterator[D]:
        """Every matching document, fetching pages as needed. Pages follow a cursor (``next_cursor``), so
        documents created, changed or deleted meanwhile never shift a page, and it is as fast at the end as at
        the start. An ``order_by`` on arrays, objects or mixed types has no cursor: those lists are fetched by
        offset instead, where documents created or deleted meanwhile can be skipped or repeated. ``limit`` is
        the page size."""
        skip = 0
        while True:
            # With a cursor the next request carries it; without one, the offset.
            page = await self.list(
                where=where,
                order_by=order_by,
                limit=limit,
                skip=None if after is not None else skip,
                after=after,
                deleted=deleted,
                file_links=file_links,
                expand=expand,
                language=language,
            )
            for doc in page.items:
                yield doc
            if not page.has_more:
                return
            if page.next_cursor is None:
                if after is not None:
                    raise RuntimeError("backd: the list has no next_cursor to continue after")
            else:
                after = page.next_cursor
            skip += limit

    async def aggregate(
        self,
        *,
        where: Where | str | None = None,
        group_by: str | Sequence[str] | None = None,
        metrics: str | Sequence[str] | None = None,
        tz: str | None = None,
        order_by: str | Sequence[str] | None = None,
        limit: int | None = None,
        language: str | None = None,
    ) -> AggregateResult:
        """Summarizes the documents the caller may read: counts and measures grouped by the fields the collection
        declares in ``aggregate:``. The read rules apply before grouping, so a total never includes a document a
        list would hide.

            result = orders.aggregate(
                group_by=["category", "paid_at:month"], tz="Europe/Madrid",
                metrics=["count", "sum:total"], order_by="-sum_total", limit=20)
            result["groups"][0]["key"]        # {"category": "shoes", "paid_at:month": "2026-09"}
            result["groups"][0]["sum_total"]  # "18234.5" (an exact decimal string)

        ``group_by``: up to 3 declared fields; a date takes a bucket (``"paid_at:month"``: ``day``, ``week``,
        ``month``, ``year``). ``metrics``: ``count`` (the default), ``count_distinct:<field>``, ``sum:<field>``,
        ``avg:<field>``, ``min:<field>``, ``max:<field>``. ``tz`` is the IANA zone of the date buckets (``UTC`` by
        default). Fails with :class:`AggregateNotEnabledError` when the collection declares no ``aggregate:``,
        :class:`InvalidAggregateError` for a wrong parameter and :class:`AggregateTooExpensiveError` when it needs
        more than allowed."""
        query: dict[str, Any] = {
            "where": where_json(where),
            "group_by": csv(group_by),
            "metrics": csv(metrics),
            "tz": tz,
            "order_by": csv(order_by),
            "limit": limit,
            "language": language,
            **self._scope(),
        }
        res = await self._client.request("GET", [*self.path, "_aggregate"], query=query)
        return cast(AggregateResult, res.data)

    async def get(
        self,
        id: str,
        *,
        deleted: Deleted | None = None,
        file_links: bool = False,
        expand: str | None = None,
    ) -> D:
        """One document; :class:`NotFoundError` if it doesn't exist or the caller may not read it. A soft-deleted
        document is only found with ``deleted="only"`` or ``"include"``. ``file_links`` adds ``url`` and
        ``expires_at`` to every file the document holds (they expire). ``expand`` returns the declared references
        named in it in ``_expanded``."""
        query = {"deleted": deleted, "file_links": True if file_links else None, "expand": expand}
        res = await self._client.request("GET", [*self.path, id], query=query)
        return cast(D, res.data)

    # -- writes ----------------------------------------------------------------------------

    async def create(
        self,
        doc: Mapping[str, Any],
        *,
        idempotency_key: str | None = None,
        team: str | None = None,
        org: str | None = None,
    ) -> D:
        """Creates a document. ``_meta`` is set by the server, and so is ``id``, unless the collection declares
        ``custom_id``: then ``doc`` carries the ``id`` (``400 id_required`` otherwise; ``409 id_taken`` if it
        exists). With an ``idempotency_key``, a repeated request returns the document created by the first one:
        the server remembers the answer for 24 hours, and the client also retries such a request itself after a
        ``429`` or ``503`` (see ``retry``)."""
        res = await self._client.request(
            "POST",
            self.path,
            body=doc,
            query=self._scope(team, org),
            headers=_idempotency(idempotency_key),
        )
        return cast(D, res.data)

    async def replace(self, id: str, doc: Mapping[str, Any], *, if_match: int | str | None = None) -> D:
        """Replaces the whole document: fields not in ``doc`` are removed. ``if_match`` makes the write fail with
        a :class:`VersionMismatchError` unless the document is still at that version."""
        res = await self._client.request("PUT", [*self.path, id], body=doc, headers=_if_match(if_match))
        return cast(D, res.data)

    async def patch(self, id: str, patch: Mapping[str, Any], *, if_match: int | str | None = None) -> D:
        """Updates some fields (JSON Merge Patch): fields in ``patch`` are set, nested objects are merged, and
        ``None`` removes a field."""
        res = await self._client.request(
            "PATCH",
            [*self.path, id],
            body=patch,
            content_type="application/merge-patch+json",
            headers=_if_match(if_match),
        )
        return cast(D, res.data)

    async def delete(self, id: str, *, purge: bool = False, if_match: int | str | None = None) -> None:
        """Deletes a document. In a collection with ``soft_delete`` it is marked deleted and can be restored;
        ``purge=True`` removes it for good (under the ``purge`` rule)."""
        await self._client.request(
            "DELETE",
            [*self.path, id],
            query={"purge": True if purge else None},
            headers=_if_match(if_match),
        )

    async def restore(self, id: str, *, if_match: int | str | None = None) -> D:
        """Brings a soft-deleted document back, as it was, at the next version. :class:`NotFoundError` if it
        isn't in the trash or the caller may not see it there."""
        res = await self._client.request("POST", [*self.path, id, "restore"], headers=_if_match(if_match))
        return cast(D, res.data)


def _page(data: Any) -> Page[Any]:
    d = cast(dict[str, Any], data)
    return Page(
        items=d["items"],
        limit=d.get("limit", 0),
        skip=d.get("skip", 0),
        has_more=bool(d.get("has_more")),
        next_cursor=d.get("next_cursor"),
        total=d.get("total"),
    )


def _scope(team: str | None, org: str | None) -> dict[str, str]:
    q: dict[str, str] = {}
    if team is not None:
        q["team"] = team
    if org is not None:
        q["org"] = org
    return q


def _idempotency(key: str | None) -> dict[str, str]:
    return {"Idempotency-Key": key} if key is not None else {}


def _if_match(v: int | str | None) -> dict[str, str]:
    return {"If-Match": if_match_value(v)} if v is not None else {}
