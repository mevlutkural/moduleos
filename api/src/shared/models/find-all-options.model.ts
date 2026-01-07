interface FindAllOptionsParams {
  page?: number;
  limit?: number;
  orderBy?: {
    field: string;
    order: 'ASC' | 'DESC';
  }[];
  search?: string;
}

export class FindAllOptions {
  constructor(
    public page?: number,
    public limit?: number,
    public orderBy?: {
      field: string;
      order: 'ASC' | 'DESC';
    }[],
    public search?: string,
  ) {}

  public static create(params: FindAllOptionsParams) {
    return new FindAllOptions(
      params.page,
      params.limit,
      params.orderBy,
      params.search,
    );
  }
}
