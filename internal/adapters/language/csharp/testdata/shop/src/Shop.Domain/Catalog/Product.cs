namespace Shop.Domain.Catalog;

public sealed partial class Product
{
    public string Name { get; set; } = "";

    /// <summary>The price after discounts.</summary>
    public partial decimal Price();
}
