"""Lists that page by themselves."""

from __future__ import annotations

from collections import deque
from typing import Callable, Deque, Generic, Iterator, List, Optional, Tuple, TypeVar

T = TypeVar("T")

#: Fetches one page: given the `next` of the page before (None for the first), its items and its own `next`.
PageFetcher = Callable[[Optional[str]], Tuple[List[T], Optional[str]]]


class PageIterator(Generic[T]):
    """Every item of a list, page after page. The next page is asked for only when the items so far are used up,
    so ``itertools.islice(client.machines.list(), 10)`` asks for no more than it needs."""

    def __init__(self, fetch: PageFetcher[T]) -> None:
        self._fetch = fetch
        self._items: Deque[T] = deque()
        self._after: Optional[str] = None
        self._done = False

    def __iter__(self) -> Iterator[T]:
        return self

    def __next__(self) -> T:
        while not self._items:
            if self._done:
                raise StopIteration
            items, after = self._fetch(self._after)
            # a `next` that does not move on would page for ever
            self._done = after is None or after == self._after
            self._after = after
            self._items.extend(items)
        return self._items.popleft()
