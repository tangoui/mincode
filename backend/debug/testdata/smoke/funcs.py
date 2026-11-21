class Calc:
    def add(self, a, b):
        return a + b


def twice(x):
    return x * 2


def main():
    calc = Calc()
    print(calc.add(twice(1), 2))


if __name__ == "__main__":
    main()
