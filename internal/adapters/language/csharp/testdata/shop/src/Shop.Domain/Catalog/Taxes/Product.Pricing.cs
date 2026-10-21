namespace Shop.Domain.Catalog;

partial class Product : IComparable<Product>
{
    public partial decimal Price() => Pricing.Discount(10m);

    public int CompareTo(Product? other) => 0;
}
