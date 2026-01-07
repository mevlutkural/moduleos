import { SelectQueryBuilder, Brackets, ObjectLiteral } from 'typeorm';
import { BadRequestException } from '@nestjs/common';
import { I18nService } from 'nestjs-i18n';
import { FindAllOptions } from '../../domain/specifications/find-all-options.model';
import { TimeRangedFindAllOptions } from '../../domain/specifications/time-ranged-find-all-options.model';

type QueryOptions = FindAllOptions | TimeRangedFindAllOptions;

export class TypeOrmQueryApplicator<Entity extends ObjectLiteral> {
  constructor(
    private readonly i18n: I18nService,
    private readonly builder: SelectQueryBuilder<Entity>,
    private readonly options: QueryOptions,
  ) {}

  applyPagination(): this {
    const page = this.options.page || 1;
    const limit = this.options.limit || 10;

    this.builder.skip((page - 1) * limit).take(limit);

    return this;
  }

  applySorting(allowedFields: string[] = []): this {
    const sortRules = this.options.orderBy;

    if (!sortRules || sortRules.length === 0) return this;

    sortRules.forEach((rule) => {
      if (allowedFields.length > 0 && !allowedFields.includes(rule.field)) {
        throw new BadRequestException(
          this.i18n.t('errorMessages.general.sortingByNotAllowed', {
            args: {
              field: rule.field,
              allowedFields: allowedFields.join(', '),
            },
          }),
        );
      }

      this.builder.addOrderBy(
        `${this.builder.alias}.${rule.field}`,
        rule.order,
      );
    });

    return this;
  }

  applyDateRange(dateColumn: string = 'created_at'): this {
    if (!(this.options instanceof TimeRangedFindAllOptions)) {
      return this;
    }

    if (this.options.startDate) {
      this.builder.andWhere(
        `${this.builder.alias}.${dateColumn} >= :startDate`,
        {
          startDate: this.options.startDate,
        },
      );
    }

    if (this.options.endDate) {
      this.builder.andWhere(`${this.builder.alias}.${dateColumn} <= :endDate`, {
        endDate: this.options.endDate,
      });
    }

    return this;
  }

  applySearch(searchFields: string[]): this {
    if (!this.options.search || searchFields.length === 0) {
      return this;
    }

    const searchTerm = this.options.search;

    this.builder.andWhere(
      new Brackets((qb) => {
        searchFields.forEach((field) => {
          qb.orWhere(`${this.builder.alias}.${field} ILIKE :search`, {
            search: `%${searchTerm}%`,
          });
        });
      }),
    );

    return this;
  }
}
