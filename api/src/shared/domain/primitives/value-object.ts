export abstract class ValueObject<T> {
  protected readonly props: T;

  protected constructor(props: T) {
    this.props = Object.freeze(props);
  }

  equals(other: ValueObject<T>): boolean {
    if (other === null || other === undefined) return false;
    if (other.props === undefined) return false;

    return this.shallowEquals(this.props, other.props);
  }

  private shallowEquals(obj1: T, obj2: T): boolean {
    if (obj1 === obj2) return true;

    if (typeof obj1 !== 'object' || typeof obj2 !== 'object')
      return obj1 === obj2;

    const keys1 = Object.keys(obj1 as object);
    const keys2 = Object.keys(obj2 as object);

    if (keys1.length !== keys2.length) return false;

    return keys1.every(
      (key) =>
        (obj1 as Record<string, unknown>)[key] ===
        (obj2 as Record<string, unknown>)[key],
    );
  }

  getValue(): T {
    return this.props;
  }
}
