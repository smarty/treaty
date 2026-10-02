from __future__ import annotations

from dataclasses import dataclass, field
from typing import Generic, TypeVar

T = TypeVar("T")
MAX_TITLE = 120


@dataclass(frozen=True)
class Book:
    """A book on a shelf.

    Books are values.
    """

    isbn: str
    title: str = ""
    tags: list[str] = field(default_factory=list)
    _cache: dict = field(default_factory=dict)

    def short_title(self, limit: int = MAX_TITLE) -> str:
        return self.title[:limit]

    @property
    def label(self) -> str:
        return f"{self.short_title()} ({self.isbn!r:>{MAX_TITLE}})"


class Shelf(Generic[T]):
    # Shelves hold books.
    def __init__(self, books: list[Book] | None = None):
        self.books: list[Book] = books or []
        self._count = 0

    def add(self, book: Book, /, *, position: int | None = None) -> None:
        self.books.append(book)
        self._count += 1

    def _reindex(self):
        pass

    @staticmethod
    def empty() -> Shelf:
        return Shelf()


def _slug(text):
    return text.lower()
