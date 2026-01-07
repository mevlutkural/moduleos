class PaginationMeta {
  constructor(
    public readonly totalRecords: number,
    public readonly totalPages: number,
    public readonly currentPage: number,
    public readonly hasNextPage: boolean,
    public readonly hasPreviousPage: boolean,
  ) {}
}

interface PaginatedParams<T> {
  records: T[];
  totalItems: number;
  currentPage: number;
  limit: number;
}

export class Paginated<T> {
  private constructor(
    public readonly records: T[],
    public readonly meta: PaginationMeta,
  ) {}

  static create<T>(params: PaginatedParams<T>): Paginated<T> {
    const totalPages = Math.ceil(params.totalItems / params.limit);
    const hasNext = params.currentPage < totalPages;
    const hasPrevious = params.currentPage > 1;

    return new Paginated(
      params.records,
      new PaginationMeta(
        params.totalItems,
        totalPages,
        params.currentPage,
        hasNext,
        hasPrevious,
      ),
    );
  }
}
