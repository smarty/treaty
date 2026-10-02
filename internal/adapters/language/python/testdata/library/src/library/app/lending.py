import library.domain
from library.domain import Book, Catalog
from ..domain.book import Shelf as BookShelf
from library.adapters import memory


class Lending:
    def __init__(self, catalog: Catalog):
        self.catalog = catalog

    async def lend(self, isbn: str) -> Book:
        book = self.catalog.find(isbn)
        shelf = BookShelf.empty()
        shelf.add(book)
        return library.domain.Book(isbn=isbn)


def default_lending() -> Lending:
    return Lending(memory.MemoryCatalog())
