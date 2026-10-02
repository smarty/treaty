import sys

from library.app.lending import default_lending


def main(argv=None):
    lending = default_lending()
    return lending


if __name__ == "__main__":
    sys.exit(main())
