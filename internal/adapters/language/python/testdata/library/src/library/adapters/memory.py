from library.domain import *
from ._internal.helpers import key


class MemoryCatalog(Catalog):
    def __init__(self):
        self._books: dict[str, Book] = {}

    def find(self, isbn):
        return self._books.get(key(isbn))

    def save(self, book):
        self._books[key(book.isbn)] = book


def key(isbn):
    return isbn.strip()
