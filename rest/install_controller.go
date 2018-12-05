package rest

import (
	"fmt"
	"github.com/jinzhu/gorm"
	"github.com/nu7hatch/gouuid"
	"go/build"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

//安装程序的接口，只有安装阶段可以访问。
type InstallController struct {
	BaseController
	uploadTokenDao    *UploadTokenDao
	downloadTokenDao  *DownloadTokenDao
	matterDao         *MatterDao
	matterService     *MatterService
	imageCacheDao     *ImageCacheDao
	imageCacheService *ImageCacheService
}

//初始化方法
func (this *InstallController) Init() {
	this.BaseController.Init()

	//手动装填本实例的Bean.
	b := CONTEXT.GetBean(this.uploadTokenDao)
	if c, ok := b.(*UploadTokenDao); ok {
		this.uploadTokenDao = c
	}

	b = CONTEXT.GetBean(this.downloadTokenDao)
	if c, ok := b.(*DownloadTokenDao); ok {
		this.downloadTokenDao = c
	}

	b = CONTEXT.GetBean(this.matterDao)
	if c, ok := b.(*MatterDao); ok {
		this.matterDao = c
	}

	b = CONTEXT.GetBean(this.matterService)
	if c, ok := b.(*MatterService); ok {
		this.matterService = c
	}

	b = CONTEXT.GetBean(this.imageCacheDao)
	if c, ok := b.(*ImageCacheDao); ok {
		this.imageCacheDao = c
	}

	b = CONTEXT.GetBean(this.imageCacheService)
	if c, ok := b.(*ImageCacheService); ok {
		this.imageCacheService = c
	}

}

//注册自己的路由。
func (this *InstallController) RegisterRoutes() map[string]func(writer http.ResponseWriter, request *http.Request) {

	routeMap := make(map[string]func(writer http.ResponseWriter, request *http.Request))

	//每个Controller需要主动注册自己的路由。
	routeMap["/api/install/verify"] = this.Wrap(this.Verify, USER_ROLE_GUEST)
	routeMap["/api/install/table/info/list"] = this.Wrap(this.TableInfoList, USER_ROLE_GUEST)
	routeMap["/api/install/create/table"] = this.Wrap(this.CreateTable, USER_ROLE_GUEST)
	routeMap["/api/install/create/admin"] = this.Wrap(this.CreateAdmin, USER_ROLE_GUEST)

	return routeMap
}

//获取数据库连接
func (this *InstallController) openDbConnection(writer http.ResponseWriter, request *http.Request) *gorm.DB {
	mysqlPortStr := request.FormValue("mysqlPort")
	mysqlHost := request.FormValue("mysqlHost")
	mysqlSchema := request.FormValue("mysqlSchema")
	mysqlUsername := request.FormValue("mysqlUsername")
	mysqlPassword := request.FormValue("mysqlPassword")

	var mysqlPort int
	if mysqlPortStr != "" {
		tmp, err := strconv.Atoi(mysqlPortStr)
		this.PanicError(err)
		mysqlPort = tmp
	}

	mysqlUrl := GetMysqlUrl(mysqlPort, mysqlHost, mysqlSchema, mysqlUsername, mysqlPassword)

	this.logger.Info("连接MySQL %s", mysqlUrl)

	var err error = nil
	db, err := gorm.Open("mysql", mysqlUrl)
	this.PanicError(err)

	return db

}

//关闭数据库连接
func (this *InstallController) closeDbConnection(db *gorm.DB) {

	if db != nil {
		err := db.Close()
		if err != nil {
			this.logger.Error("关闭数据库连接出错 %v", err)
		}
	}
}

//根据表名获取建表SQL语句
func (this *InstallController) getCreateSQLFromFile(tableName string) string {

	//1. 从当前安装目录db下去寻找建表文件。
	homePath := GetHomePath()
	filePath := homePath + "/db/" + tableName + ".sql"
	exists, err := PathExists(filePath)
	if err != nil {
		this.PanicServer("从安装目录判断建表语句文件是否存在时出错！")
	}

	//2. 从GOPATH下面去找，因为可能是开发环境
	if !exists {

		this.logger.Info("GOPATH = %s", build.Default.GOPATH)

		filePath1 := filePath
		filePath = build.Default.GOPATH + "/src/tank/build/db/" + tableName + ".sql"
		exists, err = PathExists(filePath)
		if err != nil {
			this.PanicServer("从GOPATH判断建表语句文件是否存在时出错！")
		}

		if !exists {
			this.PanicServer(fmt.Sprintf("%s 或 %s 均不存在，请检查你的安装情况。", filePath1, filePath))
		}
	}

	//读取文件内容.
	bytes, err := ioutil.ReadFile(filePath)
	this.PanicError(err)

	return string(bytes)
}

//根据表名获取建表SQL语句
func (this *InstallController) getTableMeta(gormDb *gorm.DB, entity IBase) (bool, []*gorm.StructField, []*gorm.StructField) {

	//挣扎一下，尝试获取建表语句。
	db := gormDb.Unscoped()
	scope := db.NewScope(entity)

	tableName := scope.TableName()
	modelStruct := scope.GetModelStruct()
	allFields := modelStruct.StructFields
	var missingFields = make([]*gorm.StructField, 0)

	if !scope.Dialect().HasTable(tableName) {
		missingFields = append(missingFields, allFields...)

		return false, allFields, missingFields
	} else {

		for _, field := range allFields {
			if !scope.Dialect().HasColumn(tableName, field.DBName) {
				if field.IsNormal {
					missingFields = append(missingFields, field)
				}
			}
		}

		return true, allFields, missingFields
	}

}

//验证数据库连接
func (this *InstallController) Verify(writer http.ResponseWriter, request *http.Request) *WebResult {

	db := this.openDbConnection(writer, request)
	defer this.closeDbConnection(db)

	this.logger.Info("Ping一下数据库")
	err := db.DB().Ping()
	this.PanicError(err)

	return this.Success("OK")
}

//获取需要安装的数据库表
func (this *InstallController) TableInfoList(writer http.ResponseWriter, request *http.Request) *WebResult {

	var tableNames = []IBase{&Dashboard{}, &DownloadToken{}, &Footprint{}, &ImageCache{}, &Matter{}, &Preference{}, &Session{}, UploadToken{}, &User{}}
	var installTableInfos []*InstallTableInfo

	db := this.openDbConnection(writer, request)
	defer this.closeDbConnection(db)

	for _, iBase := range tableNames {

		exist, allFields, missingFields := this.getTableMeta(db, iBase)
		installTableInfos = append(installTableInfos, &InstallTableInfo{
			Name:          iBase.TableName(),
			TableExist:    exist,
			AllFields:     allFields,
			MissingFields: missingFields,
		})

	}

	return this.Success(installTableInfos)

}

//创建缺失数据库和表
func (this *InstallController) CreateTable(writer http.ResponseWriter, request *http.Request) *WebResult {

	var tableNames = []IBase{&Dashboard{}, &DownloadToken{}, &Footprint{}, &ImageCache{}, &Matter{}, &Preference{}, &Session{}, UploadToken{}, &User{}}
	var installTableInfos []*InstallTableInfo

	db := this.openDbConnection(writer, request)
	defer this.closeDbConnection(db)

	for _, iBase := range tableNames {

		//补全缺失字段或者创建数据库表
		db1 := db.AutoMigrate(iBase)
		this.PanicError(db1.Error)

		exist, allFields, missingFields := this.getTableMeta(db, iBase)
		installTableInfos = append(installTableInfos, &InstallTableInfo{
			Name:          iBase.TableName(),
			TableExist:    exist,
			AllFields:     allFields,
			MissingFields: missingFields,
		})

	}

	return this.Success(installTableInfos)

}


//创建管理员
func (this *InstallController) CreateAdmin(writer http.ResponseWriter, request *http.Request) *WebResult {

	db := this.openDbConnection(writer, request)
	defer this.closeDbConnection(db)

	adminUsername := request.FormValue("adminUsername")
	adminEmail := request.FormValue("adminEmail")
	adminPassword := request.FormValue("adminPassword")

	//验证超级管理员的信息
	if m, _ := regexp.MatchString(`^[0-9a-zA-Z_]+$`, adminUsername); !m {
		this.PanicBadRequest(`超级管理员用户名必填，且只能包含字母，数字和'_''`)
	}

	if len(adminPassword) < 6 {
		this.PanicBadRequest(`超级管理员密码长度至少为6位`)
	}

	if adminEmail == "" {
		this.PanicBadRequest(`超级管理员邮箱必填`)
	}

	user := &User{}
	timeUUID, _ := uuid.NewV4()
	user.Uuid = string(timeUUID.String())
	user.CreateTime = time.Now()
	user.UpdateTime = time.Now()
	user.LastTime = time.Now()
	user.Sort = time.Now().UnixNano() / 1e6
	user.Role = USER_ROLE_ADMINISTRATOR
	user.Username = adminUsername
	user.Password = GetBcrypt(adminPassword)
	user.Email = adminEmail
	user.Phone = ""
	user.Gender = USER_GENDER_UNKNOWN
	user.SizeLimit = -1
	user.Status = USER_STATUS_OK

	db.Create(user)

	return this.Success("OK")

}