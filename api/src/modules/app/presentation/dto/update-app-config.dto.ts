import {
  IsOptional,
  IsNumber,
  IsString,
  IsArray,
  ValidateNested,
  Min,
  Max,
  IsIn,
} from 'class-validator';
import { Type } from 'class-transformer';

class EnvVarDto {
  @IsString()
  key: string;

  @IsString()
  value: string;
}

export class UpdateAppConfigDto {
  @IsOptional()
  @IsNumber()
  @Min(1)
  @Max(65535)
  containerPort?: number;

  @IsOptional()
  @IsNumber()
  @Min(0)
  @Max(100)
  replicas?: number;

  @IsOptional()
  @IsString()
  @IsIn(['none', 'on-failure', 'any'])
  restartPolicy?: string;

  @IsOptional()
  @IsString()
  memoryLimit?: string | null;

  @IsOptional()
  @IsString()
  cpuLimit?: string | null;

  @IsOptional()
  @IsArray()
  @ValidateNested({ each: true })
  @Type(() => EnvVarDto)
  envVars?: EnvVarDto[];
}
