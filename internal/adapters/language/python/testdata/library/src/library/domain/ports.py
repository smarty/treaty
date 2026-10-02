from typing import Protocol

from .book import Book


class Catalog(Protocol):
    def find(self, isbn: str) -> Book | None: ...

    def save(self, book: Book) -> None: ...
