class Meta {
  constructor(
    public totalRecords: number,
    public totalPages: number,
    public currentPage: number,
    public hasNextPage: boolean,
    public hasPreviousPage: boolean,
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
    public readonly meta: Meta,
  ) {}

  static create<T>(params: PaginatedParams<T>) {
    const totalPages = Math.ceil(params.totalItems / params.limit);
    const hasNext = params.currentPage < totalPages;
    const hasPrevious = params.currentPage > 1;

    return new Paginated(
      params.records,
      new Meta(
        params.totalItems,
        totalPages,
        params.currentPage,
        hasNext,
        hasPrevious,
      ),
    );
  }
}
