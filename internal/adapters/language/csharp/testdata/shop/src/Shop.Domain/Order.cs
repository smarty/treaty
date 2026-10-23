using Shop.Domain.Internal;

namespace Shop.Domain;

/// <summary>An order a customer places.</summary>
public class Order
{
    public const int Limit = 10;

    public const string Banner = """
        Thank you
        for your order
        """;

    public static readonly Order Empty = new Order(0) { Customer = "" };

    private readonly List<Line> lines = new();

    int @event;

    public Order(int id)
    {
        Guard.NotNegative(id);
        Id = id;
    }

    public int Id { get; init; }

    public required string Customer { get; set; }

    public OrderStatus Status { get; private set; }

    public Line this[int index] => lines[index];

    public void Add(Line line) => lines.Add(line);

    public void Add(Line line, int count)
    {
        for (var i = 0; i < count; i++)
        {
            Add(line);
        }
    }

    public void Ship(Line.Kind kind, // how it travels
        int days)
    {
        Status = OrderStatus.Shipped;
    }

#if NET8_0_OR_GREATER
    public string Describe() => "modern";
#else
    public string Describe() => "legacy";
#endif

    /// <summary>One product line of an order.</summary>
    public class Line
    {
        public enum Kind { Ground, Air }

        public decimal Price { get; set; }

        public decimal Total(int count) => Price * count;
    }
}
